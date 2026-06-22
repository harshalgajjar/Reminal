class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.4.4"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.4/reminal_0.4.4_darwin_arm64.tar.gz"
      sha256 "e3a7aed2fb0384e59615d122df535d8c7fc78d6b2b81d0bb15aa1430252b3cd5"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.4/reminal_0.4.4_darwin_amd64.tar.gz"
      sha256 "d6205c1c49d1a3a16b178054375690f2bde07d5749772f6fc32733cc81e70de2"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.4.4/reminal_0.4.4_linux_arm64.tar.gz"
      sha256 "3d671ec02bee1512c76caa4514d65d42d387920817d173ed321a5fc6c49f9309"
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
