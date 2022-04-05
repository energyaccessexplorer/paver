include .env

default: clean build

build:
	go fmt
	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include" \
	go build -ldflags "-s \
		-X main.tmpdir=${PAVER_TMPDIR} \
		-X main.S3KEY=${PAVER_S3KEY} \
		-X main.S3SECRET=${PAVER_S3SECRET} \
		-X main.S3PROVIDER=${PAVER_S3PROVIDER} \
		-X main.S3BUCKET=${PAVER_S3BUCKET} \
		-X main.S3DIRECTORY=${PAVER_S3DIRECTORY} \
		-X main.S3ACL=${PAVER_S3ACL}"

.export PAVER_CMD
.export PAVER_SOCKET
.export PAVER_WORKSPACE
.export PAVER_USER
	@envsubst <paver.service-tmpl >paver.service

clean:
	-rm -f paver

server:
	-@pkill -9 paver
	./${PAVER_CMD}

install:
	git pull
	bmake build
	sudo install -o root -m 755 \
		paver \
		/usr/local/bin/

	sudo install -o root -g root -m 644 \
		paver.service \
		/etc/systemd/system/

deploy:
	ssh eae "sudo systemctl stop paver.service"
	ssh eae "cd ~/paver; bmake install;"
	ssh eae "sudo systemctl daemon-reload"
	ssh eae "sudo systemctl start paver.service"

all: clean build
