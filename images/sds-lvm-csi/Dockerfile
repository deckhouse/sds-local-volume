ARG BASE_ALPINE=registry.deckhouse.io/base_images/alpine:3.16.3@sha256:5548e9172c24a1b0ca9afdd2bf534e265c94b12b36b3e0c0302f5853eaf00abb
ARG BASE_GOLANG_20_ALPINE_BUILDER=registry.deckhouse.io/base_images/golang:1.20.5-alpine3.18@sha256:51a47fb0851397db2f506c15c426735bc23de31177cbdd962880c0879d1906a4

FROM $BASE_GOLANG_20_ALPINE_BUILDER as builder

WORKDIR /go/src

ADD go.mod .
ADD go.sum .

RUN go mod download

COPY . .

WORKDIR /go/src/cmd
RUN GOOS=linux GOARCH=amd64 go build -o local-lvm-csi

FROM --platform=linux/amd64 $BASE_ALPINE

ENV DEBIAN_FRONTEND noninteractive
RUN apk add --no-cache lvm2 e2fsprogs xfsprogs 

COPY --from=builder /go/src/cmd/local-lvm-csi /go/src/cmd/local-lvm-csi

ENTRYPOINT ["/go/src/cmd/local-lvm-csi"]
