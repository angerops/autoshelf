# Canonical Homebrew formula for autoshelf.
#
# This file is the source of truth. When cutting a release, copy it to
# github.com/angerops/homebrew-tap at Formula/autoshelf.rb, then bump the
# url tag and sha256 to match the new GitHub release.
#
# Compute the sha256 of a release tarball with:
#   curl -sL https://github.com/angerops/autoshelf/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256

class Autoshelf < Formula
  desc "Set a rule once, forget it forever - YAML-driven file organizer"
  homepage "https://github.com/angerops/autoshelf"
  url "https://github.com/angerops/autoshelf/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_ME_WITH_RELEASE_TARBALL_SHA256"
  license "0BSD"
  head "https://github.com/angerops/autoshelf.git", branch: "main"

  depends_on "go" => :build

  def install
    # std_go_args handles -trimpath -o #{bin}/#{name}; we layer the version
    # injection on top so `autoshelf --version` reports the right tag.
    ldflags = %W[
      -s -w
      -X github.com/angerops/autoshelf/cmd.version=#{version}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags.join(" "))
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
    # --version must report the formula's version (proves ldflags injection).
    assert_match version.to_s, shell_output("#{bin}/autoshelf --version")

    # validate must accept a minimal config end-to-end (proves YAML loading,
    # rule normalization, and validation all work in the built binary).
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
