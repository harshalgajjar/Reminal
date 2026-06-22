class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.9"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.9/reminal_0.3.9_darwin_arm64.tar.gz"
      sha256 "c63c34be58ecaac434d59322969e9f55f7594e86660348c71f2d30bd01867b6b"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.9/reminal_0.3.9_darwin_amd64.tar.gz"
      sha256 "5a0ae0af530b110ce71eb145f14146f224051ea08e2125312bd5fdf7ce3795b6"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.9/reminal_0.3.9_linux_arm64.tar.gz"
      sha256 "cd4cd74adc24f5c0f5b7ca9f140857c8a39c96964f87639f24ea13e16db29879"
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
