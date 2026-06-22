class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.3"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.3/reminal_0.5.3_darwin_arm64.tar.gz"
      sha256 "bc2fb8a952a728acef9420f1da524aa7020f59595868bda92f86568cdf267358"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.3/reminal_0.5.3_darwin_amd64.tar.gz"
      sha256 "c18e5c98b856588542260645608c9504bce2c4e46cec267397a962b32faa3493"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.3/reminal_0.5.3_linux_arm64.tar.gz"
      sha256 "73371ffb7d6a3f251e07eaf2993e7964b41db3c2d2aff13c4371cfddd3cfd706"
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
