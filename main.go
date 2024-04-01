package main

// 包级别变量
// 需要导出的: 	则以大写字母开头驼峰命名规则
// 不需要导出的:	则以_接小写字母驼峰命名规则,用以区分函数内变量定义规则

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gomodule/redigo/redis"
	log "github.com/sirupsen/logrus"
)

const (
	BAIDU_APPID             = "you baidu appid"
	BAIDU_SECRET            = "you baidu secret"
	BAIDU_TRANSLATE_API_URI = "http://api.fanyi.baidu.com/api/trans/vip/translate"

	REDIS_HOST = "127.0.0.1:6300"

	ErrorOK = 1
	Error   = 999
)

// baidu 翻译结果 数据结构
type BaiduTranslateResultItem struct {
	Src string
	Dst string
}

type BaiduTranslateResult struct {
	From        string
	To          string
	TransResult []BaiduTranslateResultItem `json:"trans_result"`
}

// 接口参数 数据结构
type TranslateData struct {
	SrcMd5 string // md5(v)
	Src    string // 外语
	Dst    string // 翻译结果
}

type ResultMessage struct {
	Code    int
	Message string
	Data    interface{}
}

var ErrorMessages map[int]string // 错误列表
var RedisPool redis.Pool         // redis 连接池
var _phraseCacheChan chan []TranslateData

func MakeResult(code int, message string, data interface{}) *ResultMessage {
	r := &ResultMessage{}

	r.Code = code

	if len(message) <= 0 {
		if tmpMessage, ok := ErrorMessages[code]; ok {
			r.Message = tmpMessage
		}
	} else {
		r.Message = message
	}

	r.Data = data
	return r
}

// 功能函数
func generateRandom() string {
	nano := time.Now().UnixNano()
	return strconv.FormatInt(nano, 10)
}

func generateMd5(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	return hex.EncodeToString(h.Sum(nil))
}

func md5V2(str string) string {
	data := []byte(str)
	has := md5.Sum(data)
	md5str := fmt.Sprintf("%x", has)
	return md5str
}

func md5V3(str string) string {
	w := md5.New()
	io.WriteString(w, str)
	md5str := fmt.Sprintf("%x", w.Sum(nil))
	return md5str
}

// 分割数组
func splitArrayTranslateData(arr []TranslateData, num int) [][]TranslateData {

	segmens := make([][]TranslateData, 0, 20)

	tmp := make([]TranslateData, 0, num)
	for _, v := range arr {
		tmp = append(tmp, v)
		if len(tmp) == num {
			segmens = append(segmens, tmp)
			tmp = tmp[0:0]
		}
	}

	if len(tmp) > 0 {
		segmens = append(segmens, tmp)
	}

	return segmens
}

// redis 缓存短语
func runRedisCachePhrase() {
	conn := RedisPool.Get()
	defer conn.Close()

	doRedisCommand := func(phrases []TranslateData) {
		args := redis.Args{}
		for _, v := range phrases {
			args = args.Add(v.SrcMd5).Add(v.Dst)
		}

		_, err := conn.Do("MSET", args...)
		if err != nil {
			log.Error("redis mset error:", err.Error())
		}
	}

	// 分段保存到redis中
	saveToRedis := func(phrases []TranslateData) {
		segmens := splitArrayTranslateData(phrases, 300)
		for _, phraseList := range segmens {
			doRedisCommand(phraseList)
		}
	}

	for v := range _phraseCacheChan {
		go saveToRedis(v)
	}
}

// 缓存短语
func redisCachePhrase(phrases []TranslateData) {
	_phraseCacheChan <- phrases
}

func getPhraseCacheFromRedis(phrases []TranslateData) {
	conn := RedisPool.Get()
	defer conn.Close()

	doRedisCommand := func(phrasesList []TranslateData, num int) {
		args := redis.Args{}
		for _, v := range phrasesList {
			args = args.Add(v.SrcMd5)
		}

		res, err := redis.Strings(conn.Do("MGET", args...))
		if err != nil {
			log.Error("redis mset error:", err.Error())
		}

		phrasesCount := len(phrasesList)
		for i, tmpV := range res {
			phrases[i+(num*phrasesCount)].Dst = tmpV
		}
	}

	segmens := splitArrayTranslateData(phrases, 300)
	for i, phraseList := range segmens {
		doRedisCommand(phraseList, i)
	}
}

// BAIDU api 翻译
func doBaiduTranslate(query string) *BaiduTranslateResult {
	salt := generateRandom()
	from := "jp"
	to := "zh"
	str := BAIDU_APPID + query + salt + BAIDU_SECRET
	sign := generateMd5(str)
	postData := "q=" + query + "&from=" + from + "&to=" + to + "&appid=" + BAIDU_APPID + "&salt=" + salt + "&sign=" + sign

	reader := bytes.NewBufferString(postData)
	request, err := http.NewRequest("POST", BAIDU_TRANSLATE_API_URI, reader)
	if err != nil {
		log.Error(err.Error())
		return nil
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		log.Error(err.Error())
		return nil
	}

	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error(err.Error())
		return nil
	}

	result := BaiduTranslateResult{}
	err = json.Unmarshal(respBytes, &result)
	if err != nil {
		log.Error(err.Error(), string(respBytes))
		return nil
	}

	return &result
}

// JP to CN Http Request
func jp2cnHander(c *gin.Context) {

	phrases := &[]TranslateData{}
	if err := c.BindJSON(phrases); err != nil {
		c.JSON(200, MakeResult(Error, "参数错误", nil))
		return
	}

	// 先从redis 内获翻译结果
	getPhraseCacheFromRedis(*phrases)

	// 已翻译过的
	translatedPhrases := &[]TranslateData{}

	// 未翻译过的
	untranslatedPhrases := &[]TranslateData{}

	query := ""
	for _, v := range *phrases {
		if len(v.Dst) == 0 {
			query += v.Src + "\n"
			*untranslatedPhrases = append(*untranslatedPhrases, v)
		} else {
			*translatedPhrases = append(*translatedPhrases, v)
		}
	}

	log.Info("untranslatedPhrases: ", len(*untranslatedPhrases))
	log.Info("translatedPhrases: ", len(*translatedPhrases))

	// 调用 baidu 翻译
	if len(query) > 0 {
		log.Info("call baidu 翻译，词数: ", len(*untranslatedPhrases))

		// 去掉最后一个\n符号
		query = query[:len(query)-1]

		// 调用baidu 获取翻译结果
		result := doBaiduTranslate(query)
		if result == nil {
			c.JSON(200, MakeResult(Error, "翻译出错", nil))
			return
		}

		// 以顺序的方式，组织翻译结果
		if len(*untranslatedPhrases) == len(result.TransResult) {
			for i, v := range result.TransResult {

				if len(v.Dst) == 0 {
					log.Debug("%v", v)
				}

				(*untranslatedPhrases)[i].Dst = v.Dst
			}
		} else {
			// 以md5为key的方式，组织翻译结果
			tmpMd5PhrasesMap := make(map[string]*TranslateData)
			for _, v := range *untranslatedPhrases {
				tmpMd5PhrasesMap[v.SrcMd5] = &v
			}

			for _, dstV := range result.TransResult {
				// (*phrases)[i].V = v.Dst
				key := generateMd5(dstV.Src)
				if r, ok := tmpMd5PhrasesMap[key]; ok {
					r.Dst = dstV.Dst
				}
			}
		}

		redisCachePhrase(*untranslatedPhrases)
	}

	// 连接，从redis翻译的结果，与baidu翻译的结果
	*translatedPhrases = append(*translatedPhrases, *untranslatedPhrases...)
	c.JSON(200, MakeResult(ErrorOK, "", translatedPhrases))
}

// 初始化
func init() {

	// 初始化错误列表
	ErrorMessages = make(map[int]string)
	ErrorMessages[ErrorOK] = "ok"
	ErrorMessages[Error] = "错误"

	// 初始化redis
	RedisPool = redis.Pool{
		MaxIdle:     64,
		MaxActive:   128,
		IdleTimeout: 360,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", REDIS_HOST)
		},
	}

	// 初始化 redis cache chan
	_phraseCacheChan = make(chan []TranslateData, 2000)
}

// 入口
func main() {

	// 初始化redis 短语缓存
	go runRedisCachePhrase()

	// 初始化 web 框架
	r := gin.Default()
	apiGroup := r.Group("/translate_api")

	// 翻译
	// 日语 --> 中文
	apiGroup.POST("/jp_cn", jp2cnHander)

	r.Run(":8080")
}
