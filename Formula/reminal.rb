class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.0.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.1/reminal_1.0.1_darwin_arm64.tar.gz"
      sha256 "fafa40ae48dbb052a47ecea70a2f27834970a5261f8ef20b91a98b2d0a323462"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.1/reminal_1.0.1_darwin_amd64.tar.gz"
      sha256 "36bad51f9391a6292169520201b23eb3a1a18e86fd2632b040b51e525dd0317c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.1/reminal_1.0.1_linux_arm64.tar.gz"
      sha256 "e9e4271e9abbf3d780b328bb5165a516e4421bf50a0e3d8b4b337fbffe631d2a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.0.1/reminal_1.0.1_linux_amd64.tar.gz"
      sha256 "f5876489781748093a5d4a69c97b0e7a092936f04d0da59b23c3ea00b4410cec"
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
