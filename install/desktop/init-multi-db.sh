#!/bin/sh
# Runs once, on first init of the postgres data volume (docker-entrypoint-initdb.d
# convention). Creates one extra logical database per service beyond the default
# POSTGRES_DB, all owned by the same POSTGRES_USER, so the desktop stack can run a
# single postgres:16 container instead of three.
set -e

for db in blerg_board blerg_runner; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE $db OWNER $POSTGRES_USER;
EOSQL
done
