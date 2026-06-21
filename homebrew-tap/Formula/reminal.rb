class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.3.2"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.2/reminal_0.3.2_darwin_arm64.tar.gz"
      sha256 "c45d3c1ff03020cbebfdfa7959b190f3115bb95c0e754abb80d6eaf438c78004"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.2/reminal_0.3.2_darwin_amd64.tar.gz"
      sha256 "ec09cfed6352d207ce53e2daa404257fc96432a1d5b138bd598345c8d0441b27"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.3.2/reminal_0.3.2_linux_arm64.tar.gz"
      sha256 "9a2681dd8b1828bcab5baf275c7ebbe45d2c3573232e05e21dbeb434c0de2780"
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
