FROM golang:1.21-alpine

RUN apk add --no-cache \
    python3 \
    py3-pip \
    bash \
    git \
    gcc \
    libc-dev \
    python3-dev

RUN python3 -m venv /app/venv
ENV PATH="/app/venv/bin:$PATH"
ADD ./examples/requirements.txt /app/
RUN pip3 install -r /app/requirements.txt
RUN rm /app/requirements.txt

ADD main.go go.mod index.html /app/
RUN mkdir -p /app/examples
#RUN mkdir -p /app/jobs
#RUN sed -ie 's|const jobsDir = "./examples/"|const jobsDir = "./jobs/"|' /app/main.go 

WORKDIR /app
RUN go get repeater
RUN go build
RUN rm main.go go.mod index.html

EXPOSE 8080

ENTRYPOINT ["/app/repeater"]