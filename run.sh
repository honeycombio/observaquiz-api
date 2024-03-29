#!/bin/sh
if [ -z "${HONEYCOMB_API_KEY}" ]; then
    echo "HONEYCOMB_API_KEY Environment variable isn't set"
    exit 1
fi

if [ ! -e "environment.json" ]; then
    echo "environment.json file doesn't exist"
    exit 1
fi

echo "Starting the collector..." # the function will 502 if the collector is down!
docker-compose -f local-collector/docker-compose.yml up -d

echo "Building..."
./build.sh

echo "Running lambda simulator..."
sam local start-api --env-vars environment.json --docker-network local-collector_collector_net --skip-pull-image

echo "Go to http_tests/questions.http in VSCode to click on some requests to try"
