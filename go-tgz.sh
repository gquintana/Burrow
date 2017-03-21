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
chmod -R 0755 ${workdir}/bin
cp config/* ${workdir}/config
chmod 0755 ${workdir}/config
chmod 0644 ${workdir}/config/*
chmod 0600 ${workdir}/config/user.cfg
tar -czf burrow-$version.tar.gz ${workdir}
rm -Rf ${workdir}
