FROM alpine

ADD bin/cf_bot_linux_amd64 /

RUN chmod +x /cf_bot_linux_amd64

RUN apk --update upgrade && \
    apk add curl ca-certificates && \
    update-ca-certificates && \
    rm -rf /var/cache/apk/*

EXPOSE 8080

CMD /cf_bot_linux_amd64
