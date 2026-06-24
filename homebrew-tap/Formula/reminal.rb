class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.16"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.16/reminal_0.7.16_darwin_arm64.tar.gz"
      sha256 "35de39211636f17497f7614999c477ae31f711fbffa5faabfa6ceb9a40b62eb4"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.16/reminal_0.7.16_darwin_amd64.tar.gz"
      sha256 "45a649d56a6a1c311ffb2e40f55a8e7cf084af71cf8e923c52f5b6c116046002"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.16/reminal_0.7.16_linux_arm64.tar.gz"
      sha256 "12069689df941436f7ec278b2cc972c0b72c09fb5a0bb9de610ee8498f438d37"
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
