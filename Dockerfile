FROM golang:1.21-alpine

RUN apk add --no-cache \
    python3 \
    py3-pip \
    py3-requests \
    bash \
    git \
    gcc \
    libc-dev \
    python3-dev

RUN python3 -m venv /app/venv
ENV PATH="/app/venv/bin:$PATH"
#COPY ./requirements.txt /app/requirements.txt
#RUN pip3 install -Ur /app/requirements.txt
RUN pip3 install clickhouse-connect

WORKDIR /app
RUN git clone https://github.com/andrewbrdk/Repeater
RUN cp ./Repeater/main.go ./Repeater/go.mod ./
RUN rm -r ./Repeater
RUN mkdir -p /app/examples
#RUN mkdir -p /app/jobs
#RUN sed -ie 's|const jobsDir = "./examples/"|const jobsDir = "./jobs/"|' ./main.go 
RUN go get repeater
RUN go build 

EXPOSE 8080

ENTRYPOINT ["/app/repeater"]