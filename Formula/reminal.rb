class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.0/reminal_0.7.0_darwin_arm64.tar.gz"
      sha256 "9b0ddf98bc6eb86cc18d78b9fea51e0b92a831283d4808e173b880809976f4d4"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.0/reminal_0.7.0_darwin_amd64.tar.gz"
      sha256 "952031a6c32eda9eb3e3f7b4c7be65638292ba97005f4549a714be440802c622"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.0/reminal_0.7.0_linux_arm64.tar.gz"
      sha256 "decfc52982c55a9605ea684322b1055b3cb58a5c95c7c6a768936e493d4855fa"
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
