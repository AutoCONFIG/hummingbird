#  ----------------------------------------------------------------------------------
#  Copyright 2018 hummingbird, J.
#  Copyright 2018 Cavium
#
#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#   Unless required by applicable law or agreed to in writing, software
#   distributed under the License is distributed on an "AS IS" BASIS,
#   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#   See the License for the specific language governing permissions and
#   limitations under the License.
# 
#  ----------------------------------------------------------------------------------

# go.mod 已升级到 go 1.24（paho v1.5.1 等安全升级要求），构建镜像同步升级
# Docker Hub 直连不可达时，默认走 daocloud 镜像源；可覆盖：--build-arg BUILDER_BASE=golang:1.24-alpine
ARG BUILDER_BASE=docker.m.daocloud.io/library/golang:1.24-alpine

FROM ${BUILDER_BASE} AS builder

# CGO 编译（sqlite）需要 gcc/musl-dev；Makefile 需要 make；go build 需要 git
RUN apk add --no-cache gcc musl-dev make git

WORKDIR /edge

ENV GOPROXY=https://goproxy.cn,direct
# go.sum 被 .gitignore 忽略，构建时需允许 go 自动补全校验条目
ENV GOFLAGS=-mod=mod

COPY . .

RUN make cmd/hummingbird-core/hummingbird-core

#Next image - Copy built Go binary into new workspace
FROM docker.m.daocloud.io/library/alpine:3.20

ENV APP_PORT=58081
ENV WEB_PORT=3000
EXPOSE $APP_PORT
EXPOSE $WEB_PORT

WORKDIR /var/bin/


COPY --from=builder /edge/cmd/hummingbird-core/hummingbird-core /bin/
COPY --from=builder /edge/cmd/hummingbird-ui/build /var/bin/cmd/hummingbird-ui/build

CMD ["/bin/sh", "-c", "/bin/hummingbird-core --confdir=/etc/hummingbird-core/configuration.toml"]
