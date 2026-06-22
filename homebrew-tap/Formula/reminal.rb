class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_darwin_arm64.tar.gz"
      sha256 "37a754e37d661f36d360e67c750493136a843444fb10ec51820d0d720bdb76dc"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_darwin_amd64.tar.gz"
      sha256 "58cbe5464356734cf9403177a8222588704d810e1710a9bf50a695abc7c8e1da"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.4/reminal_0.3.4_linux_arm64.tar.gz"
      sha256 "0808bab9c2b257b842f116827cdbc1c11389dd5800bf36a06737b3ce24edb7e9"
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
