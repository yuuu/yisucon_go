NAME     := isuwitter
SRCS    := $(shell find . -type f -name '*.go')

bin/$(NAME): $(SRCS)
	GOOS=linux GOARCH=amd64 go build -o $(NAME)
	scp $(NAME) ec2-user@192.168.33.10:/home/ec2-user/work/$(NAME)

.PHONY: glide
dep:
	glide install

.PHONY: clean
clean:
	rm -f $(NAME)
