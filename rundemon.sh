#!/bin/bash
set -e

case "$1" in
    start)
        if [ "$2" == '-d' ]; then
            if [ "$3" == '-log' ]; then
                nohup ./translate_api_server > translate_api_server.log 2>&1 & echo $! >> translate_api_server.txt
            else
                nohup ./translate_api_server > /dev/null 2>&1 & echo $! >> translate_api_server.txt
            fi
            echo "run translate_api_server by demon, pid: `cat translate_api_server.txt`"
        else
            ./translate_api_server
        fi
        ;;

    stop)
        echo "stop"
        kill -9 `cat translate_api_server.txt`
        rm -rf translate_api_server.txt

        ;;
    status)
        ps aux | grep 'translate_api_server'
        ;;
esac