class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.8"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.8/reminal_0.7.8_darwin_arm64.tar.gz"
      sha256 "005b9cdd86a5ca98ba82ed5f009bf37805c49dfee876e80702c97b600e1bf21e"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.8/reminal_0.7.8_darwin_amd64.tar.gz"
      sha256 "e964d9881a8ac38e69ea27fe7793f7e8fa92d6ee663f8e1799ee4d3e7ec37274"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.8/reminal_0.7.8_linux_arm64.tar.gz"
      sha256 "6ff382ad592a5b81593ec03fe87ceebd422a7612c9dc8c52a06dc7e2fad17660"
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
