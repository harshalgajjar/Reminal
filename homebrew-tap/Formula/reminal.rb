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
      sha256 "9f8291bd4fcf1607a92635d6ee8a642df954d2e673d8450cbc597668ef9e3339"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.8/reminal_0.3.8_darwin_amd64.tar.gz"
      sha256 "de857c661ed12610ac5db2f3fbce108fb9752919516f493edde04bbcbaf5cab3"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.8/reminal_0.3.8_linux_arm64.tar.gz"
      sha256 "3edfb6cfec4d136890608322d7133f11fa5567742c10979d62e5e3b0c5371198"
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
