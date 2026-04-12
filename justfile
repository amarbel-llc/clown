default: build

build: build-nix

build-nix:
    nix build --show-trace

clean:
    rm -rf result
