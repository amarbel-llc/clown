# home-manager module for `programs.juggler` — see FDR-0010 /
# docs/plans/2026-05-18-juggler-control-plane.md.
#
# Single-instance only. The daemon binds one Unix domain socket at
# $HOME/.local/state/juggler/control.sock and tracks the running
# llama-server children in memory. Multi-instance juggler isn't a
# thing — one daemon manages many llama-server children.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (lib)
    mkEnableOption
    mkIf
    mkOption
    types
    ;

  cfg = config.programs.juggler;

  # The llama-server control-plane daemon, invoked as `juggler daemon`. The
  # daemon stays clown-side; the job platform it once shared a binary with was
  # extracted into the standalone github.com/amarbel-llc/ringmaster module.
  # Wire `package` to a clown output carrying `bin/juggler` (e.g.
  # clown.packages.<system>.juggler).
  binPath = "${cfg.package}/bin/juggler";

  # Single launcher script used by both systemd and launchd. Resolves
  # XDG_STATE_HOME and XDG_LOG_HOME with their POSIX-default fallbacks,
  # ensures the parent dirs exist (the daemon will create the socket
  # itself), then execs. Putting this in a shell script — rather than
  # baking the paths into the unit file — keeps the runtime expansion
  # of $HOME / $XDG_STATE_HOME / $XDG_LOG_HOME working on macOS where
  # launchd does NOT expand env vars in ProgramArguments.
  launcher = pkgs.writeShellScript "juggler-launch" ''
    set -eu
    : "''${HOME:?HOME must be set}"
    : "''${XDG_STATE_HOME:=$HOME/.local/state}"
    : "''${XDG_LOG_HOME:=$HOME/.local/log}"
    mkdir -p -m 0700 "$XDG_STATE_HOME/juggler"
    mkdir -p -m 0755 "$XDG_LOG_HOME"
    exec ${binPath} daemon
  '';
in
{
  options.programs.juggler = {
    enable = mkEnableOption "juggler llama-server control plane";

    package = mkOption {
      type = types.package;
      description = ''
        The package providing the `juggler` binary whose `daemon` verb runs
        the llama-server control plane. The clown flake exposes one as
        `packages.<system>.juggler`.
      '';
    };
  };

  config = mkIf cfg.enable {
    # Linux: systemd user service. Restart on failure so a crashed
    # daemon comes back without the user noticing.
    systemd.user.services.juggler = mkIf pkgs.stdenv.isLinux {
      Unit = {
        Description = "Juggler (llama-server control plane)";
        Documentation = "https://github.com/amarbel-llc/clown";
      };
      Service = {
        ExecStart = "${launcher}";
        Restart = "always";
        RestartSec = 3;
        StandardOutput = "journal";
        StandardError = "journal";
      };
      Install.WantedBy = [ "default.target" ];
    };

    # Darwin: launchd agent. KeepAlive { Crashed = true } restarts the
    # daemon on crash but leaves it alone on clean exits (so the user
    # can kill it for upgrades without it respawning instantly).
    # StandardErrorPath must be an absolute path at eval time —
    # launchd doesn't expand `$HOME` in plist contexts. Resolve
    # through `config.home.homeDirectory` so the type-check passes.
    launchd.agents.juggler = mkIf pkgs.stdenv.isDarwin {
      enable = true;
      config = {
        ProgramArguments = [ "${launcher}" ];
        KeepAlive = {
          Crashed = true;
          SuccessfulExit = false;
        };
        RunAtLoad = true;
        ProcessType = "Background";
        StandardOutPath = "${config.home.homeDirectory}/.local/log/juggler.log";
        StandardErrorPath = "${config.home.homeDirectory}/.local/log/juggler.log";
      };
    };
  };
}
