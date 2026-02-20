#!/bin/sh

if [ ! -d .git ] ; then
    echo "Please run this script from the root of the repository"
    exit 1
fi

if [ ! -d deploy ] ; then
    mkdir deploy
fi

rm ./deploy/*
GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -o ./deploy/bootstrap ./cmd/api/*.go
zip -j ./deploy/api.zip ./deploy/bootstrap
rm ./deploy/bootstrap
GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -o ./deploy/bootstrap ./cmd/deepchecks_callback/*.go
zip -j ./deploy/deepchecks_callback.zip ./deploy/bootstrap
rm ./deploy/bootstrap

cd infra
pulumi up --yes
