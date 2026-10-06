# go.nix — this module's dependencies (FDR 0008); go.mod, gomod2nix.toml and
# the package graph are rendered or derived from it inside nix. Edit through
# the escape hatch (godyn-go) or by hand.
{
  flakeInputs = {
    "code.linenisgreat.com/purse-first/libs/dewey" = {
      input = "purse-first";
      subPath = "libs/dewey";
    };
    "code.linenisgreat.com/ringmaster" = {
      input = "ringmaster";
    };
    "code.linenisgreat.com/tommy" = {
      input = "tommy";
    };
  };
  go = "1.26";
  module = "code.linenisgreat.com/clown";
  replace = { };
  require = {
    "github.com/BurntSushi/toml" = {
      go = "1.18";
      hash = "sha256-ptdUJvuc21ixeLt+M5way/na3aCnCO4MYHWulWp8NEY=";
      version = "v1.6.0";
    };
    "github.com/atotto/clipboard" = {
      hash = "sha256-ZZ7U5X0gWOu8zcjZcWbcpzGOGdycwq0TjTFh/eZHjXk=";
      indirect = true;
      version = "v0.1.4";
    };
    "github.com/aymanbagabas/go-osc52/v2" = {
      go = "1.16";
      hash = "sha256-6Bp0jBZ6npvsYcKZGHHIUSVSTAMEyieweAX2YAKDjjg=";
      indirect = true;
      version = "v2.0.1";
    };
    "github.com/catppuccin/go" = {
      go = "1.19";
      hash = "sha256-otcMhI62ezoKGqzG7Owi/NROep7O0voJxp6bwXYg9+Q=";
      indirect = true;
      version = "v0.3.0";
    };
    "github.com/charmbracelet/bubbles" = {
      go = "1.23.0";
      hash = "sha256-UpFrV82xWIydxE4wPEQ3GK56+L9d/PDCkBAOYTifKlw=";
      version = "v0.21.1-0.20250623103423-23b8fd6302d7";
    };
    "github.com/charmbracelet/bubbletea" = {
      go = "1.23.0";
      hash = "sha256-79MpQk+92RDXnNc2hSct8EDf0AhV6MZpvFMmmrVC/bs=";
      version = "v1.3.6";
    };
    "github.com/charmbracelet/colorprofile" = {
      go = "1.18";
      hash = "sha256-D9E/bMOyLXAUVOHA1/6o3i+vVmLfwIMOWib6sU7A6+Q=";
      indirect = true;
      version = "v0.2.3-0.20250311203215-f60798e515dc";
    };
    "github.com/charmbracelet/harmonica" = {
      go = "1.16";
      hash = "sha256-fi5N0IXhSbbYHdSZFngCfpT4kdiEaKedqj8YpnlvX0o=";
      indirect = true;
      version = "v0.2.0";
    };
    "github.com/charmbracelet/huh" = {
      go = "1.23.0";
      hash = "sha256-vDqcsW9uBPDt0FaOA7Bij+Q9CkozggstOZ0r557TaC4=";
      version = "v1.0.0";
    };
    "github.com/charmbracelet/lipgloss" = {
      go = "1.18";
      hash = "sha256-RHsRT2EZ1nDOElxAK+6/DC9XAaGVjDTgPvRh3pyCfY4=";
      version = "v1.1.0";
    };
    "github.com/charmbracelet/x/ansi" = {
      go = "1.23.0";
      hash = "sha256-Bzum17p7UQZeNxL155Pho/+GXj1DElB9Bp3O194CYf8=";
      indirect = true;
      version = "v0.9.3";
    };
    "github.com/charmbracelet/x/cellbuf" = {
      go = "1.18";
      hash = "sha256-ubgBd82jcS5L4i6rCFLOB1BXGrDEu6wWlciJA0gsH2g=";
      indirect = true;
      version = "v0.0.13";
    };
    "github.com/charmbracelet/x/exp/strings" = {
      go = "1.19";
      hash = "sha256-NWe8LHXUtrrABWFhmAzLNYAZyJIwN3C/T2OdaInjl9E=";
      indirect = true;
      version = "v0.0.0-20240722160745-212f7b056ed0";
    };
    "github.com/charmbracelet/x/term" = {
      go = "1.18";
      hash = "sha256-VBkCZLI90PhMasftGw3403IqoV7d3E5WEGAIVrN5xQM=";
      indirect = true;
      version = "v0.2.1";
    };
    "github.com/dustin/go-humanize" = {
      go = "1.16";
      hash = "sha256-yuvxYYngpfVkUg9yAmG99IUVmADTQA0tMbBXe0Fq0Mc=";
      indirect = true;
      version = "v1.0.1";
    };
    "github.com/erikgeiser/coninput" = {
      go = "1.16";
      hash = "sha256-OWSqN1+IoL73rWXWdbbcahZu8n2al90Y3eT5Z0vgHvU=";
      indirect = true;
      version = "v0.0.0-20211004153227-1c3628e74d0f";
    };
    "github.com/itchyny/gojq" = {
      go = "1.24.0";
      hash = "sha256-egaBNHKKzwDwaUN4GT+Xvt11Nz6ojNMSIrXbcEisyI4=";
      version = "v0.12.19";
    };
    "github.com/itchyny/timefmt-go" = {
      go = "1.24";
      hash = "sha256-6h57JsmWju1QAx+mI9HZ+XceAeRnfLcA5HMWCHdymYE=";
      indirect = true;
      version = "v0.1.8";
    };
    "github.com/lucasb-eyer/go-colorful" = {
      go = "1.12";
      hash = "sha256-Gg9dDJFCTaHrKHRR1SrJgZ8fWieJkybljybkI9x0gyE=";
      indirect = true;
      version = "v1.2.0";
    };
    "github.com/mattn/go-isatty" = {
      go = "1.15";
      hash = "sha256-qhw9hWtU5wnyFyuMbKx+7RB8ckQaFQ8D+8GKPkN3HHQ=";
      indirect = true;
      version = "v0.0.20";
    };
    "github.com/mattn/go-localereader" = {
      hash = "sha256-JlWckeGaWG+bXK8l8WEdZqmSiTwCA8b1qbmBKa/Fj3E=";
      indirect = true;
      version = "v0.0.1";
    };
    "github.com/mattn/go-runewidth" = {
      go = "1.9";
      hash = "sha256-NC+ntvwIpqDNmXb7aixcg09il80ygq6JAnW0Gb5b/DQ=";
      indirect = true;
      version = "v0.0.16";
    };
    "github.com/mitchellh/hashstructure/v2" = {
      go = "1.14";
      hash = "sha256-O4Yw4pPQECWe8DoVDIH2nUMN8Zl8waS7/O1sv18M2Xs=";
      indirect = true;
      version = "v2.0.2";
    };
    "github.com/muesli/ansi" = {
      go = "1.17";
      hash = "sha256-qRKn0Bh2yvP0QxeEMeZe11Vz0BPFIkVcleKsPeybKMs=";
      indirect = true;
      version = "v0.0.0-20230316100256-276c6243b2f6";
    };
    "github.com/muesli/cancelreader" = {
      go = "1.17";
      hash = "sha256-uEPpzwRJBJsQWBw6M71FDfgJuR7n55d/7IV8MO+rpwQ=";
      indirect = true;
      version = "v0.2.2";
    };
    "github.com/muesli/termenv" = {
      go = "1.17";
      hash = "sha256-hGo275DJlyLtcifSLpWnk8jardOksdeX9lH4lBeE3gI=";
      indirect = true;
      version = "v0.16.0";
    };
    "github.com/rivo/uniseg" = {
      go = "1.18";
      hash = "sha256-rDcdNYH6ZD8KouyyiZCUEy8JrjOQoAkxHBhugrfHjFo=";
      indirect = true;
      version = "v0.4.7";
    };
    "github.com/sahilm/fuzzy" = {
      hash = "sha256-f2VsDI6G+V2w31tSDzbZPi9EI2E7jRV6Aq8yeOorSZY=";
      indirect = true;
      version = "v0.1.1";
    };
    "github.com/xo/terminfo" = {
      go = "1.19";
      hash = "sha256-GyCDxxMQhXA3Pi/TsWXpA8cX5akEoZV7CFx4RO3rARU=";
      indirect = true;
      version = "v0.0.0-20220910002029-abceb7e1c41e";
    };
    "golang.org/x/sync" = {
      go = "1.23.0";
      hash = "sha256-Jf4ehm8H8YAWY6mM151RI5CbG7JcOFtmN0AZx4bE3UE=";
      indirect = true;
      version = "v0.15.0";
    };
    "golang.org/x/sys" = {
      go = "1.25.0";
      hash = "sha256-aDQXqSTZES2l/132PBxhZN4ywldpPyfm7LByYCHzzwM=";
      version = "v0.43.0";
    };
    "golang.org/x/term" = {
      go = "1.25.0";
      hash = "sha256-FCiDvAfq7dgBGQuiDYDFJbj/JPawhrmPF2qdUEftQ1c=";
      version = "v0.42.0";
    };
    "golang.org/x/text" = {
      go = "1.23.0";
      hash = "sha256-TiYX1K4DYpP1dEV06whOm43xyOntjrPFi+VAdncoeCY=";
      indirect = true;
      version = "v0.23.0";
    };
  };
}
