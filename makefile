include .env

default: clean build server

build:
	go fmt
	CGO_LDFLAGS="-L/usr/local/lib -lgdal" \
	CGO_CFLAGS="-I/usr/local/include" \
	go build -ldflags "-s \
		-X main.jwtkey=${JWTKEY} \
		-X main.tmpdir=${TMPDIR} \
		-X main.S3KEY=${S3KEY} \
		-X main.S3SECRET=${S3SECRET} \
		-X main.S3PROVIDER=${S3PROVIDER} \
		-X main.S3BUCKET=${S3BUCKET} \
		-X main.S3DIRECTORY=${S3DIRECTORY} \
		-X main.S3ACL=${S3ACL}"

.export CMD
.export SOCKET
.export WORKSPACE
.export USER
	@envsubst <paver.service-tmpl >paver.service

clean:
	-rm -f paver

server:
	-@pkill -9 paver
	@rm -f ${SOCKET}
	@./${CMD}

install:
	git pull
	bmake build
	sudo install -o root -m 755 \
		paver paver-check \
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
