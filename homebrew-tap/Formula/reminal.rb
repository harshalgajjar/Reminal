class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.6.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_darwin_arm64.tar.gz"
      sha256 "295a4f8d234d9e36a860d81cd30190e05692547526a313c57c85ba0081590431"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_darwin_amd64.tar.gz"
      sha256 "7ecb271a1a57a0dfd0e140eca65961d37a40bdb5f66f5120090e36102ad2b2c4"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_linux_arm64.tar.gz"
      sha256 "0a6ae2abfd8e9390a8cd8abc4e715a028b5f7e4d8659e15dea0deb08a96459ba"
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
