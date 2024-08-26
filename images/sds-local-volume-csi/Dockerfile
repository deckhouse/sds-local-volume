ARG BASE_ALPINE=registry.deckhouse.io/base_images/alpine:3.16.9@sha256:fd0e79d78108625846e5f95b2da118bbf941e9ccb43492cee672ceab0e60e65c
ARG BASE_GOLANG_ALPINE_BUILDER=registry.deckhouse.io/base_images/golang:1.22.3-alpine@sha256:dbf216b880b802c22e3f4f2ef0a78396b4a9a6983cb9b767c5efc351ebf946b0

FROM $BASE_GOLANG_ALPINE_BUILDER as builder

WORKDIR /go/src

ADD go.mod .
ADD go.sum .

RUN go mod download

COPY . .

WORKDIR /go/src/cmd
RUN GOOS=linux GOARCH=amd64 go build -o sds-local-volume-csi

FROM --platform=linux/amd64 $BASE_ALPINE

ENV DEBIAN_FRONTEND noninteractive
RUN apk add --no-cache lvm2 e2fsprogs e2fsprogs-extra xfsprogs xfsprogs-extra blkid

COPY --from=builder /go/src/cmd/sds-local-volume-csi /go/src/cmd/sds-local-volume-csi

ENTRYPOINT ["/go/src/cmd/sds-local-volume-csi"]
