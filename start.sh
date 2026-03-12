#!/bin/sh

# 启动 supervisor
exec /usr/bin/supervisord -c /etc/supervisord.conf
