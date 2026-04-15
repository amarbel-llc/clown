default: build

build: build-nix

build-nix:
    nix build --show-trace

clean:
    rm -rf result

# Bump all flake inputs and rebuild to verify
bump: && build
    nix flake update
