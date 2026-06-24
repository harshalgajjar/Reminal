class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.18"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.18/reminal_0.7.18_darwin_arm64.tar.gz"
      sha256 "b5fd52fca3cdc7851b31e9d490834a89ed502aba8eacf5320519b0251b8ea568"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.18/reminal_0.7.18_darwin_amd64.tar.gz"
      sha256 "97442f4ecd9b2803a23a4c9b5af85bb19366f0ec58bc293254801cad8dffc49c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.18/reminal_0.7.18_linux_arm64.tar.gz"
      sha256 "11c3b98156b81fb199064ead3992ae207414d3e78f7b370cf26a5460717a0384"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
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
