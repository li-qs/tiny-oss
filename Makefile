NAME=myoss
BIN_DIR=$(CURDIR)/build

APP_DIR=$(CURDIR)/cmd/app

APP_NAME=$(NAME)

PLATFORMS=linux-amd64 darwin-amd64 windows-amd64 darwin-arm64

.PHONY: all clean app

all: app

app: $(PLATFORMS:%=app-%)

app-%:
	@mkdir -p $(BIN_DIR)
	GOOS=$(word 1,$(subst -, ,$*)) \
	GOARCH=$(word 2,$(subst -, ,$*)) \
	CGO_ENABLED=0 \
	go build -o $(BIN_DIR)/$(APP_NAME)-$* $(APP_DIR)

clean:
	rm -rf $(BUILD_DIR)
