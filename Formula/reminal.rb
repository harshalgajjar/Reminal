class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.0/reminal_0.3.0_darwin_arm64.tar.gz"
      sha256 "1025b50eb7bd0f034ddbd521191131f02a5f02502977c3ed8da38cb7c1793abe"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.0/reminal_0.3.0_darwin_amd64.tar.gz"
      sha256 "1764bc249912b8ac3860b3f7fc0cdf988a8af62472cea65d24d6f2aec134f701"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.0/reminal_0.3.0_linux_arm64.tar.gz"
      sha256 "f7406f6ec028301440c87e916496d86ed57af590ae627109a2b9e13d572cc331"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
