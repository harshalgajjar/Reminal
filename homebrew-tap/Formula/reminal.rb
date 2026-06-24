class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.8.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.5/reminal_0.8.5_darwin_arm64.tar.gz"
      sha256 "29e6cde805193c1ce62a1ad4d042e8bff1c882015b76089b71ae46e08fde35b2"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.5/reminal_0.8.5_darwin_amd64.tar.gz"
      sha256 "2f4ab4c564a8663dd9fd5b7f3459b3d73ae6b1f6b80e1e722080c89cc0c55a44"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.8.5/reminal_0.8.5_linux_arm64.tar.gz"
      sha256 "7a15bec13712fbba7b519329bc2d0b2b917c18f61b28a9f34b54043d0ab8d0a8"
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
