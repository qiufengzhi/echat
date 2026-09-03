#!/bin/sh
# SpiceDB 关系元组存储库：postgres 容器首次初始化数据卷时由官方 entrypoint 执行
# 幂等创建 echat_spicedb 库，spicedb serve 使用该库自建关系元组表
set -eu

if ! psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER:-echat}" --dbname postgres \
  -tAc "SELECT 1 FROM pg_database WHERE datname = 'echat_spicedb'" | grep -q 1; then
  psql -v ON_ERROR_STOP=1 --username "${POSTGRES_USER:-echat}" --dbname postgres \
    -c "CREATE DATABASE echat_spicedb OWNER \"${POSTGRES_USER:-echat}\""
fi