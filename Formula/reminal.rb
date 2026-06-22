class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.4.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.1/reminal_0.4.1_darwin_arm64.tar.gz"
      sha256 "657348655395df694f987fd95281c6c3bb9fb0edf82a727b8eb4dfcd3987ddf6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.1/reminal_0.4.1_darwin_amd64.tar.gz"
      sha256 "7fc724c5d6394ab1c83b22b7a0fef4d0b5c03595ffd365f7892256258876013e"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.1/reminal_0.4.1_linux_arm64.tar.gz"
      sha256 "41f454637512f29f3c49b75c03aa57ca2dbbbb9efdba67d670c23c5948da07b1"
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
