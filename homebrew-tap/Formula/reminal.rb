class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.1/reminal_0.7.1_darwin_arm64.tar.gz"
      sha256 "f5ca3937dc5eb32c221a4514f287a13df70be869fa3dd14bb3580169d9ce34df"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.1/reminal_0.7.1_darwin_amd64.tar.gz"
      sha256 "fc7b8d608310a864adba6528b199d2b2966983fc064d3d60a4a4fcfb43203c00"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.1/reminal_0.7.1_linux_arm64.tar.gz"
      sha256 "cfc7ae34a272a2bdeff6fe0b810162a12a949eba747bc67837ba528e618a8f76"
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
