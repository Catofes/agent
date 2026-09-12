#!/bin/sh
set -eu

mkdir -p "$MPLCONFIGDIR"
cp /usr/local/share/classroom/matplotlibrc "$MPLCONFIGDIR/matplotlibrc"
exec "$@"
