docker_compose("./local-collector/docker-compose.yml")

dc_resource("collector",
	labels = ["runny-things"],
)

local_resource("API",
	cmd = "GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -o ./dist/api cmd/api/*.go",
	deps = ["./cmd/api"],
	labels = ["buildy-things"]
)

local_resource("Callback",
	cmd = "GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -o ./dist/deepchecks_callback cmd/deepchecks_callback/*.go",
	deps = ["./cmd/deepchecks_callback"],
	labels = ["buildy-things"]
)

local_resource("Lambda Simulator",
	serve_cmd = "sam local start-api --env-vars environment.json --docker-network local-collector_collector_net",
	deps = ["./template.yaml", "./environment.json", "./cmd"],
	resource_deps = ["collector", "API", "Callback"],
	links = ["http://127.0.0.1:3000/"],
	labels = ["runny-things"],
)

local_resource("fake backend-router",
	serve_cmd = "cd ../observaquiz-ui/test/fake-backend && npm run impersonate",
	labels = ["runny-things"],
)

local_resource("UI",
	serve_cmd = "cd ../observaquiz-ui && npm run serve",
	resource_deps = ["fake backend-router"],
	labels = ["runny-things"],
)
