.PHONY: help all build install lint lint-go test tidy demo

#: lint, test, and build (default)
all: lint test tidy build

define PROMPT
	@echo
	@echo "**********************************************************"
	@echo "*"
	@echo "*   $(1)"
	@echo "*"
	@echo "**********************************************************"
	@echo
endef

#: compile for the current platform
build:
	$(call PROMPT, $@)
	go build -o loader .

#: install the binary in $GOPATH/bin
install:
	$(call PROMPT, $@)
	go install

#: run all linters
lint: lint-go

#: run Go linters
lint-go:
	$(call PROMPT, $@)
	golangci-lint run

#: run all tests
test:
	$(call PROMPT, $@)
	go test -race ./...

#: tidy go.mod and go.sum
tidy:
	$(call PROMPT, $@)
	go mod tidy

#: run the demo tape to create the gif
demo: demo.gif

demo.gif: demo.tape build
	$(call PROMPT, $@)
	PS1="$$ " vhs $<

#: print Makefile targets and short descriptions
help:
	@echo "make targets:\n"
	@awk '/^#:[[:space:]]/ { sub(/^#:[[:space:]]*/, ""); desc=$$0; next } \
		/^[[:space:]]*$$/ { next } \
		/^#/ { next } \
		/^[a-zA-Z][a-zA-Z0-9_.-]*:/ { \
			if (desc != "") { \
				split($$0, a, ":"); \
				tgt=a[1]; \
				gsub(/^[[:space:]]+|[[:space:]]+$$/, "", tgt); \
				printf "  %-18s %s\n", tgt, desc; \
				desc="" \
			} \
		}' $(firstword $(MAKEFILE_LIST))
