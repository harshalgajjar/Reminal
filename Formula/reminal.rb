class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.2.3"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.3/reminal_1.2.3_darwin_arm64.tar.gz"
      sha256 "6b4dec2083f03165e88b0992746940589f0bcccae3a6477cb224830d1f424974"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.3/reminal_1.2.3_darwin_amd64.tar.gz"
      sha256 "e140f3540ea2250845074d01372764c822ce840369c3c62fbec006202593fc22"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.3/reminal_1.2.3_linux_arm64.tar.gz"
      sha256 "3cdd9446dc0e093967b32fd09b79eabd7c3b0e5656834a9bac3e6704ed49168f"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.2.3/reminal_1.2.3_linux_amd64.tar.gz"
      sha256 "98e5c62bf01289cad41125efb2af1bffca20d8981fad24e8b5456ee6341e2628"
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
