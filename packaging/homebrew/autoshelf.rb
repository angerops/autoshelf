# Canonical Homebrew formula for autoshelf.
#
# This file is the source of truth. When cutting a release, copy it to
# github.com/angerops/homebrew-tap at Formula/autoshelf.rb, then bump the
# version field, the four release-asset URLs, and the four sha256 values.
#
# The four sha256 values come straight from the release workflow's
# checksums.txt artifact. After a v* tag triggers the release, fetch them
# with one curl:
#
#   curl -sL https://github.com/angerops/autoshelf/releases/download/v0.1.0/checksums.txt
#
# Each line is `<sha256>  autoshelf-v0.1.0-<target>.tar.gz`. Paste the four
# digests into the matching on_arm / on_intel blocks below.

class Autoshelf < Formula
  desc "Set a rule once, forget it forever - YAML-driven file organizer"
  homepage "https://github.com/angerops/autoshelf"
  version "0.1.0"
  license "0BSD"

  # Stable installs pull the pre-built binary for the user's OS + arch from
  # the GitHub Release the workflow produced. No Go toolchain required for
  # end users; install is "fetch tarball, verify sha, drop binary in bin".
  on_macos do
    on_arm do
      url "https://github.com/angerops/autoshelf/releases/download/v0.1.0/autoshelf-v0.1.0-darwin-arm64.tar.gz"
      sha256 "f8778cdaba6f0a6c3e746b09f4761c269d16e9e316235143c721c8ea11e40081"
    end
    on_intel do
      url "https://github.com/angerops/autoshelf/releases/download/v0.1.0/autoshelf-v0.1.0-darwin-amd64.tar.gz"
      sha256 "d2590a688c2d2cdcba94c4565cdb124803caf167ba9d29c0080857158eab54ba"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/angerops/autoshelf/releases/download/v0.1.0/autoshelf-v0.1.0-linux-arm64.tar.gz"
      sha256 "80e042d3d407bfa992d17fad31d51533f2226eba87758ab89d0ae799e4ddc624"
    end
    on_intel do
      url "https://github.com/angerops/autoshelf/releases/download/v0.1.0/autoshelf-v0.1.0-linux-amd64.tar.gz"
      sha256 "a5d4b26f5dd0340ce91b819756de9fb29f03e4098ba5be865c898d2bb53b3cda"
    end
  end

  # `brew install --HEAD angerops/tap/autoshelf` is still the build-from-source
  # escape hatch. Only this path needs Go on the user's machine.
  head do
    url "https://github.com/angerops/autoshelf.git", branch: "main"
    depends_on "go" => :build
  end

  def install
    if build.head?
      # Source build for --HEAD. std_go_args handles -trimpath and -o; the
      # ldflags injection keeps `autoshelf --version` returning something
      # meaningful even when the user installed off a moving branch.
      ldflags = %W[
        -s -w
        -X github.com/angerops/autoshelf/cmd.version=#{version}-HEAD
      ]
      system "go", "build", *std_go_args(ldflags: ldflags.join(" "))
    else
      # Binary install. The workflow's archive layout is:
      #   autoshelf            (the binary)
      #   LICENSE
      #   README.md
      bin.install "autoshelf"
    end
  end

  # Homebrew's service block generates a launchd plist (macOS) or systemd
  # user unit (Linux). After `brew install`, the user runs:
  #
  #   brew services start autoshelf
  #
  # ...and brew takes care of the equivalent of the README's manual plist.
  # Logs land at #{var}/log/autoshelf.log, which `autoshelf log` finds
  # automatically by probing HOMEBREW_PREFIX/var/log first.
  service do
    run [opt_bin/"autoshelf", "run"]
    keep_alive true
    log_path var/"log/autoshelf.log"
    error_log_path var/"log/autoshelf.log"
    working_dir Dir.home
  end

  test do
    # --version must report the formula's version (proves the right binary
    # landed for stable installs, and that ldflags injection worked under
    # --HEAD).
    assert_match version.to_s, shell_output("#{bin}/autoshelf --version")

    # validate must accept a minimal config end-to-end (proves YAML loading,
    # rule normalization, and validation all work in the installed binary).
    (testpath/"autoshelf.yaml").write <<~YAML
      watches:
        - path: #{testpath}
          rules:
            - name: test
              match:
                globs: ["*.txt"]
              destination: #{testpath}/sorted
    YAML
    system "#{bin}/autoshelf", "validate", "-c", "#{testpath}/autoshelf.yaml"
  end
end
