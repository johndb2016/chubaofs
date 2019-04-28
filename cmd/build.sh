#!/usr/bin/env bash
cd $GOPATH/src/github.com/chubaofs/chubaofs/cmd
export LD_LIBRARY_PATH=/usr/local/lib:$LD_LIBRARY_PATH
CGO_CFLAGS="-I/usr/local/include" CGO_LDFLAGS="-L/usr/local/lib -lrocksdb -lstdc++ -lm -lz -lbz2 -lsnappy " go build -ldflags "-X main.Version=`git rev-parse HEAD`"
