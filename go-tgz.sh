#!/bin/bash

version=$1
if [ "$version" == "" ]; then
  version=$(date +%Y.%m.%d)
fi
go install
workdir="burrow-$version"
mkdir ${workdir}
mkdir ${workdir}/bin
mkdir ${workdir}/config
cp $GOPATH/bin/Burrow ${workdir}/bin/burrow
cp config/* ${workdir}/config
tar -czf burrow-$version.tar.gz ${workdir}
rm -Rf ${workdir}
