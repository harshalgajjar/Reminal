class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.8"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.8/reminal_0.3.8_darwin_arm64.tar.gz"
      sha256 "0081fc7cafb99bd258a02347395e55fb4d1adc9a279c4841cf67153fe8509c1e"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.8/reminal_0.3.8_darwin_amd64.tar.gz"
      sha256 "9f81691868096f9dc242355b263a13634f17e64a1a88cd0dab55563dec07030f"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.8/reminal_0.3.8_linux_arm64.tar.gz"
      sha256 "7713888831248caeca828981af5268394683e9dfa9e3bc1d871b8a8affeb352e"
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
