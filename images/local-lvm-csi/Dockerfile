#ARG BASE_UBUNTU=registry.deckhouse.io/base_images/ubuntu:jammy-20221130@sha256:c14c3b1242536729ce5227ff833144977b4e378723858fb73a4cf40ea6daaf6a
ARG BASE_UBUNTU=ubuntu:jammy
ARG BASE_GOLANG_19_BULLSEYE=registry.deckhouse.io/base_images/golang:1.19.3-bullseye@sha256:3d68e9eabd09f01f5851297902f2756ee2456a2e28b212e553315a3ddfcffe4b

FROM $BASE_GOLANG_19_BULLSEYE as builder

WORKDIR /go/src

ADD go.mod .
ADD go.sum .

RUN go mod download

COPY . .

WORKDIR /go/src/cmd
RUN GOOS=linux GOARCH=amd64 go build -o local-lvm-csi

FROM --platform=linux/amd64 $BASE_UBUNTU
COPY --from=builder /go/src/cmd/local-lvm-csi /go/src/cmd/local-lvm-csi

ENV DEBIAN_FRONTEND noninteractive
RUN apt-get update \
 && apt-get -y install lvm2 \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*

ENTRYPOINT ["/go/src/cmd/local-lvm-csi"]
