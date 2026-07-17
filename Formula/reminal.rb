class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.8.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.0/reminal_1.8.0_darwin_arm64.tar.gz"
      sha256 "8a418d3c8dc0bd8accd1b04ed843823976524d2f14ba429c7f67105ef3669898"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.0/reminal_1.8.0_darwin_amd64.tar.gz"
      sha256 "04346dc0cf6571d4300a3c250e643849de46f4c962faececdb3d088cbef88c2b"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.0/reminal_1.8.0_linux_arm64.tar.gz"
      sha256 "2d1080b1d3a6a734585cb49c55055aa51d022bcf88a8ad1dcd4429c22a366887"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.0/reminal_1.8.0_linux_amd64.tar.gz"
      sha256 "98375308f401f867e3e99c43146d5b3ae42c76287d51df9ff07d5686da1e6389"
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
