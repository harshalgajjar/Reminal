class Reminal < Formula
  desc "Remote terminal access from any browser — no SSH, no port forwarding"
  homepage "https://github.com/reminal/reminal"
  version "0.1.0"
  license "MIT"

  head do
    url "https://github.com/reminal/reminal.git", branch: "main"
  end

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/reminal/reminal/releases/download/v0.1.0/reminal_0.1.0_darwin_arm64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_ARM64"
    else
      url "https://github.com/reminal/reminal/releases/download/v0.1.0/reminal_0.1.0_darwin_amd64.tar.gz"
      sha256 "PLACEHOLDER_SHA256_AMD64"
    end
  end

  def install
    if build.head?
      system "go", "build", "-ldflags", "-s -w", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def caveats
    <<~EOS
      reminal connects to a free Cloudflare relay automatically — no setup needed.

      Just run:  reminal

      For local development:
        reminal relay
        REMINAL_LOCAL=1 reminal
    EOS
  end

  test do
    assert_match "0.1.0", shell_output("#{bin}/reminal version")
  end
end
