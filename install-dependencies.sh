#! /usr/bin/env bash

LIBRAW_VERSION=LibRaw-0.19.0-Beta3

wget -O libraw.tar.gz https://www.libraw.org/data/$LIBRAW_VERSION.tar.gz
tar xvf libraw.tar.gz
pushd $LIBRAW_VERSION

./configure
make -j4
sudo make install

popd

rm -rf $LIBRAW_VERSION
rm -f libraw.tar.gz
