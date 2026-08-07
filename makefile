default: clean build

-include .env

# WebSocket Origin allowlist baked into the binary at build time (socket.go); .env overrides.
PAVER_SOCKET_ACCEPT_PATTERN ?= *.energyaccessexplorer.org

export PAVER_SERVER := ${PAVER_SERVER}
export PAVER_SOCKET := ${PAVER_SOCKET}
export PAVER_WORKDIR := ${PAVER_WORKDIR}
export PAVER_USER := ${PAVER_USER}
export PAVER_CMD := paver \
	-pubkey ${PAVER_PUBKEY} \
	-socket ${PAVER_SOCKET} \
	-buckets ${PAVER_BUCKETS}

export PAVER_STAGING_ORCHESTRATOR_SOCKET := ${PAVER_STAGING_ORCHESTRATOR_SOCKET}
export PAVER_STAGING_ORCHESTRATOR_CMD := paver-staging-orchestrator \
	-pubkey ${PAVER_PUBKEY} \
	-buckets ${PAVER_BUCKETS} \
	-socket ${PAVER_STAGING_ORCHESTRATOR_SOCKET} \
	-tickets-path ${PAVER_TICKETS_PATH} \
	-run-dir ${PAVER_STAGING_ORCHESTRATOR_RUNDIR} \
	-deploy-token-file ${PAVER_DEPLOY_TOKEN_FILE}

run:
	-@ pkill -9 paver
	./${PAVER_CMD}

build:
	go get
	go fmt

	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include -I/usr/include/gdal" \
	go build -ldflags "-s \
		-X main.SOCKET_ACCEPT_PATTERN=${PAVER_SOCKET_ACCEPT_PATTERN} \
		-X main.BUILD_DATE=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)"

	envsubst <paver.service-tmpl >paver.service
	cat paver.service

build-orchestrator:
	go build -ldflags "-s" -o paver-staging-orchestrator ./staging-orchestrator

	envsubst <paver-staging-orchestrator.service-tmpl >paver-staging-orchestrator.service
	cat paver-staging-orchestrator.service

clean:
	-rm -f paver paver.service paver-staging-orchestrator paver-staging-orchestrator.service

install: build
	sudo install -o root -m 755 \
		paver \
		/usr/local/bin/

	sudo install -o root -g root -m 644 \
		paver.service \
		/etc/systemd/system/

install-orchestrator: build-orchestrator
	sudo install -o root -m 755 \
		paver-staging-orchestrator \
		/usr/local/bin/

	sudo install -o root -g root -m 644 \
		paver-staging-orchestrator.service \
		/etc/systemd/system/

deploy:
	ssh ${PAVER_SERVER} "cd ${PAVER_SRCDIR}; git stash; git pull; touch deploy.diff; patch -p1 <deploy.diff;"
	ssh ${PAVER_SERVER} "sudo systemctl stop paver.service"
	ssh ${PAVER_SERVER} "cd ${PAVER_SRCDIR}; make install;"
	ssh ${PAVER_SERVER} "sudo systemctl daemon-reload"
	ssh ${PAVER_SERVER} "sudo systemctl start paver.service"

deploy-orchestrator:
	ssh ${PAVER_SERVER} "cd ${PAVER_SRCDIR}; git stash; git pull; touch deploy.diff; patch -p1 <deploy.diff;"
	ssh ${PAVER_SERVER} "sudo systemctl stop paver-staging-orchestrator.service"
	ssh ${PAVER_SERVER} "cd ${PAVER_SRCDIR}; make install-orchestrator;"
	ssh ${PAVER_SERVER} "sudo systemctl daemon-reload"
	ssh ${PAVER_SERVER} "sudo systemctl start paver-staging-orchestrator.service"

all: clean build build-orchestrator
