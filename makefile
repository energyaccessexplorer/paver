include .env

CMD = paver -server -role admin -role master -role root
SOCKET = /tmp/paver-server.sock

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
	@envsubst <paver.service-tmpl >paver.service

clean:
	-rm -f paver

server:
	-@pkill -9 paver
	@rm -rf /tmp/paver-server.sock
	@./${CMD}

cli:
	@./paver -cli -c s3put \
		-i ${POLYGON_SHP}

	@./paver -cli -c admin_boundaries \
		-i ${POLYGON_SHP} \
		-g DistrictID

	@./paver -cli -c vectors_routine \
		-i ${LINES_SHP} \
		-r ${POLYGON_GEOJSON} \
		-g full_id

	@./paver -cli -c vectors_clipped_routine \
		-i ${POINTS_GEOJSON} \
		-r ${POLYGON_GEOJSON} \
		-g iso

	@./paver -cli -c csv \
		-i ${POLYGON_SHP} \
		-s DistrictID \
		-s Radio

	@ls -lh outputs

install:
	git pull
	bmake build
	sudo install -o root -g ubuntu -m 755 \
		paver paver-check \
		/usr/local/bin/

	sudo install -o root -g root -m 644 \
		paver.service \
		/etc/systemd/system/

deploy:
	-ssh eae "sudo pkill -9 paver"
	ssh eae "sudo rm -f /tmp/paver-server.sock"

	ssh eae "cd ~/paver; bmake install;"
	ssh eae "${CMD}" &

all: clean build
